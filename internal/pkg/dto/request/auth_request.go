package request

// This file has been modified for the KestrelDesk project.

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type PasswordResetRequest struct {
	Identifier string `json:"identifier"`
}

type PasswordResetConfirmRequest struct {
	Token           string `json:"token"`
	NewPassword     string `json:"newPassword"`
	ConfirmPassword string `json:"confirmPassword"`
}

type UpdateProfileRequest struct {
	Nickname string  `json:"nickname"`
	Avatar   string  `json:"avatar"`
	Email    *string `json:"email"`
}

type WxWorkExchangeRequest struct {
	Ticket string `json:"ticket"`
}

type OIDCExchangeRequest struct {
	Ticket string `json:"ticket"`
}
