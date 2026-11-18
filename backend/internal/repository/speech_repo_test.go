//go:build unit

// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"testing"
)

func TestSpeechReadyMeterAndReservationCommitTogether(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			db, mock, e := sqlmock.New()
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			repo := &usageBillingRepository{db: db}
			cmd := &service.UsageBillingCommand{RequestID: "s_1", UserID: 1, APIKeyID: 2, AccountID: 3, Model: "asr", BillingType: service.BillingTypePoints, PointsReservationID: "reservation", PointsGroupID: 4, PointsPerUSD: 100, PointsCostUSD: .02}
			meter := &service.SpeechMeterRecord{ID: "s_1", SessionID: "s", ReservationID: "reservation", Quantity: 2, Points: 2, Unit: "seconds", State: "ready", Command: cmd}
			mock.ExpectBegin()
			mock.ExpectExec("UPDATE points_reservations SET command=").WillReturnResult(sqlmock.NewResult(0, 1))
			insert := mock.ExpectExec("INSERT INTO speech_meter_segments")
			if fail {
				insert.WillReturnError(errors.New("storage offline"))
				mock.ExpectRollback()
			} else {
				insert.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			e = repo.PutSpeechMeter(context.Background(), meter)
			if (e != nil) != fail {
				t.Fatal(e)
			}
			if e = mock.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestSpeechTicketRedeemIsAtomicAndExpiredRejected(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	repo := &usageBillingRepository{db: db}
	mock.ExpectQuery("UPDATE speech_sessions SET state='active'.*state='created'.*expires_at>NOW").WithArgs("expired").WillReturnError(errors.New("no matching ticket"))
	if _, e = repo.RedeemSpeechSession(context.Background(), "expired"); e == nil {
		t.Fatal("失效票据被接受")
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

// 恢复器无法读取语音用量时仍回收普通预扣，但必须在数据库条件中保护已有消费。
func TestSpeechRecoveryStorageFailureDoesNotReleaseConsumedReservations(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectQuery("SELECT id,state,command FROM points_reservations").WillReturnRows(sqlmock.NewRows([]string{"id", "state", "command"}).AddRow("consumed", "active", nil))
	mock.ExpectQuery("SELECT m.id,m.session_id").WillReturnError(errors.New("speech storage offline"))
	mock.ExpectExec("WITH released AS.*NOT EXISTS.*speech_meter_segments.*quantity>0").WithArgs("consumed").WillReturnResult(sqlmock.NewResult(0, 0))
	if err := repo.RecoverPoints(context.Background()); err == nil {
		t.Fatal("账务恢复失败被忽略")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSpeechTicketRetainsSelectedLanguage(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	repo := &usageBillingRepository{db: db}
	mock.ExpectQuery("UPDATE speech_sessions SET state='active'.*RETURNING.*language").WithArgs("ticket").WillReturnRows(sqlmock.NewRows([]string{"id", "key", "user", "group", "model", "language"}).AddRow("session", 1, 2, 3, []byte("{}"), "en-US"))
	session, e := repo.RedeemSpeechSession(context.Background(), "ticket")
	if e != nil {
		t.Fatal(e)
	}
	if session.Language != "en-US" {
		t.Fatal("识别语言丢失")
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
