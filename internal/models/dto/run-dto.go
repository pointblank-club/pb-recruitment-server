package dto

type RunRequest struct {
	Language string `json:"language" validate:"required"`
	Code     string `json:"code" validate:"required"`
	Stdin    string `json:"stdin"`
}

type RunResponse struct {
	Status        string `json:"status"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	CompileOutput string `json:"compile_output"`
	Runtime       int64  `json:"runtime"` // milliseconds
	Memory        int64  `json:"memory"`  // KiB
}
