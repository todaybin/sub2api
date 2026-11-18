//go:build unit

// Copyright (c) 2026 WorkMesh. 保留所有权利。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"testing"
)

func TestSpeechReconciliationReleaseAndAuditAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		db, m, e := sqlmock.New()
		if e != nil {
			t.Fatal(e)
		}
		repo := &usageBillingRepository{db: db}
		m.ExpectBegin()
		m.ExpectQuery("SELECT reservation_id").WithArgs("meter").WillReturnRows(sqlmock.NewRows([]string{"reservation"}).AddRow("reservation"))
		m.ExpectQuery("SELECT points_per_usd").WithArgs("reservation").WillReturnRows(sqlmock.NewRows([]string{"rate"}).AddRow(100))
		m.ExpectQuery("SELECT m.session_id").WithArgs("meter").WillReturnRows(sqlmock.NewRows([]string{"session", "quantity", "unit", "state", "supplier", "model", "key", "user", "group", "eligible"}).AddRow("session", 0, "characters", "pending", "unknown", []byte("{}"), 1, 2, 3, true))
		m.ExpectQuery("SELECT decision,quantity,evidence").WithArgs("meter").WillReturnError(sql.ErrNoRows)
		m.ExpectExec("UPDATE speech_meter_segments SET state='released'").WithArgs("meter").WillReturnResult(sqlmock.NewResult(0, 1))
		m.ExpectExec("WITH released AS").WithArgs("reservation").WillReturnResult(sqlmock.NewResult(0, 1))
		insert := m.ExpectExec("INSERT INTO speech_meter_reconciliations").WithArgs("meter", int64(9), "released", 0.0, "bill-reference")
		if fail {
			insert.WillReturnError(errors.New("audit offline"))
			m.ExpectRollback()
		} else {
			insert.WillReturnResult(sqlmock.NewResult(0, 1))
			m.ExpectCommit()
		}
		e = repo.ReconcileSpeech(context.Background(), "meter", service.SpeechReconciliation{Decision: "released", ActorID: 9, Evidence: "bill-reference"})
		if (e != nil) != fail {
			t.Fatal(e)
		}
		if e = m.ExpectationsWereMet(); e != nil {
			t.Fatal(e)
		}
		db.Close()
	}
}
func TestSpeechReconciliationIdempotentAndConflicts(t *testing.T) {
	for _, same := range []bool{true, false} {
		db, m, e := sqlmock.New()
		if e != nil {
			t.Fatal(e)
		}
		repo := &usageBillingRepository{db: db}
		m.ExpectBegin()
		m.ExpectQuery("SELECT reservation_id").WillReturnRows(sqlmock.NewRows([]string{"reservation"}).AddRow("free"))
		m.ExpectQuery("SELECT m.session_id").WillReturnRows(sqlmock.NewRows([]string{"session", "quantity", "unit", "state", "supplier", "model", "key", "user", "group", "eligible"}).AddRow("session", 0, "characters", "released", "confirmed", []byte("{}"), 1, 2, 3, true))
		m.ExpectQuery("SELECT decision,quantity,evidence").WillReturnRows(sqlmock.NewRows([]string{"decision", "quantity", "evidence"}).AddRow("released", 0, "reference"))
		if same {
			m.ExpectCommit()
		} else {
			m.ExpectRollback()
		}
		evidence := "reference"
		if !same {
			evidence = "different"
		}
		e = repo.ReconcileSpeech(context.Background(), "meter", service.SpeechReconciliation{Decision: "released", ActorID: 9, Evidence: evidence})
		if (e == nil) != same {
			t.Fatal(e)
		}
		if e = m.ExpectationsWereMet(); e != nil {
			t.Fatal(e)
		}
		db.Close()
	}
}
func TestSpeechUnknownReservationProtectedFromDirectRelease(t *testing.T) {
	repo, m := newRedeemAdjustmentRepoMock(t)
	m.ExpectExec("WITH released AS.*NOT EXISTS.*supplier_state IN").WithArgs("unknown").WillReturnResult(sqlmock.NewResult(0, 0))
	if e := repo.ReleasePoints(context.Background(), "unknown"); e != nil {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
