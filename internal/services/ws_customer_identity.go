package services

import "strconv"

func (s *wsService) customerTopic(customerID int64) string {
	if customerID <= 0 {
		return ""
	}
	return "customer:" + strconv.FormatInt(customerID, 10)
}

func (s *wsService) IsCustomerOnline(customerID int64) bool {
	if customerID <= 0 {
		return false
	}
	return s.manager.HasTopic(s.customerTopic(customerID))
}
