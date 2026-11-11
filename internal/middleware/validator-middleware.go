package middleware

import (
	"app/internal/common"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ValidationErrors struct {
	Errors []ValidationError `json:"errors"`
}

type CustomValidator struct {
	validator *validator.Validate
}

// NewValidator creates a new validator instance
func NewValidator() *CustomValidator {
	v := validator.New()

	// Register custom tag name function to use json tag names in errors
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
		if name == "-" {
			return ""
		}
		return name
	})

	return &CustomValidator{
		validator: v,
	}
}

// Validate validates a struct using validator.v10 package
func (cv *CustomValidator) Validate(i interface{}) error {
	return cv.validator.Struct(i)
}

// ValidateRequest middleware validates the request body against the provided struct
func ValidateRequest(i interface{}) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Create a new instance of the struct
			req := reflect.New(reflect.TypeOf(i).Elem()).Interface()

			// Bind request to struct
			if err := c.Bind(req); err != nil {
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) && httpErr.Code == http.StatusRequestEntityTooLarge {
					return err
				}
				return c.JSON(http.StatusBadRequest, map[string]string{
					"error": "Invalid request format",
				})
			}

			// Validate the struct
			v := NewValidator()
			if err := v.Validate(req); err != nil {
				// Process validation errors
				validationErrors := ValidationErrors{
					Errors: []ValidationError{},
				}

				for _, err := range err.(validator.ValidationErrors) {
					validationErrors.Errors = append(validationErrors.Errors, ValidationError{
						Field:   err.Field(),
						Message: formatValidationError(err),
					})
				}

				return c.JSON(http.StatusBadRequest, validationErrors)
			}

			// Set validated struct to context
			c.Set(common.VALIDATED_REQUEST_BODY, req)

			return next(c)
		}
	}
}

// Standalone validation function for use in controllers
func Validate(c echo.Context, i interface{}) error {
	if err := c.Bind(i); err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusRequestEntityTooLarge {
			return err
		}
		return echo.NewHTTPError(http.StatusBadRequest, "Invalid request format")
	}

	v := NewValidator()
	if err := v.Validate(i); err != nil {
		validationErrors := ValidationErrors{
			Errors: []ValidationError{},
		}

		for _, err := range err.(validator.ValidationErrors) {
			validationErrors.Errors = append(validationErrors.Errors, ValidationError{
				Field:   err.Field(),
				Message: formatValidationError(err),
			})
		}

		return echo.NewHTTPError(http.StatusBadRequest, validationErrors)
	}

	return nil
}

// formatValidationError creates a user-friendly error message from validation errors
func formatValidationError(err validator.FieldError) string {
	switch err.Tag() {
	case "required":
		return "This field is required"
	case "email":
		return "Invalid email address"
	case "min":
		return "Value must be greater than " + err.Param()
	case "max":
		return "Value must be less than " + err.Param()
	case "gt":
		return "Value must be greater than " + err.Param()
	case "lt":
		return "Value must be less than " + err.Param()
	default:
		return "Validation failed on condition: " + err.Tag()
	}
}
