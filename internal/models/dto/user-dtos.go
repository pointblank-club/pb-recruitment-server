package dto

type CreateUserRequest struct {
	Name         string `json:"name" validate:"required,max=200"`
	USN          string `json:"usn" validate:"required,max=32"`
	MobileNumber string `json:"mobile_number" validate:"required,len=10"`
	CurrentYear  int    `json:"current_year" validate:"required,min=1,max=3"`
	Department   string `json:"department" validate:"required,max=200"`
}

type UpdateUserProfileRequest struct {
	Name         string `json:"name" validate:"required,max=200"`
	MobileNumber string `json:"mobile_number" validate:"required,len=10"`
	Department   string `json:"department" validate:"required,max=200"`
}

type SignupRequest struct {
	Name         string `json:"name" validate:"required,max=200"`
	Email        string `json:"email" validate:"required,email,max=254"`
	Password     string `json:"password" validate:"required,min=6,max=128"`
	USN          string `json:"usn" validate:"required,max=32"`
	MobileNumber string `json:"mobile_number" validate:"required,len=10"`
	CurrentYear  int    `json:"current_year" validate:"required,min=1,max=3"`
	Department   string `json:"department" validate:"required,max=200"`
}

type SignupResponse struct {
	UserID string `json:"user_id"`
}
