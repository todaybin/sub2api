package service

// CodeBuddyBrandName 按账号区域返回品牌名称。
func CodeBuddyBrandName(account *Account) string {
	if CodeBuddyRegionForAccount(account) == CodeBuddyRegionInternational {
		return "WorkBuddy"
	}
	return "CodeBuddy"
}
