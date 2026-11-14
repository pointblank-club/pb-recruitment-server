package middleware

import (
	"app/internal/common"
	"app/internal/models/dto"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// ValidateUserInput validates USN, mobile number, current year, and department
// against college formats before any rate limits or worker slots are consumed.
func ValidateUserInput() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rawBody := c.Get(common.VALIDATED_REQUEST_BODY)
			if rawBody == nil {
				return next(c)
			}

			var (
				usn         string
				mobile      string
				currentYear int
				department  string
				normalize   func(cleanUSN, cleanMobile string)
			)

			switch body := rawBody.(type) {
			case *dto.SignupRequest:
				usn = body.USN
				mobile = body.MobileNumber
				currentYear = body.CurrentYear
				department = body.Department
				normalize = func(cleanUSN, cleanMobile string) {
					body.USN = cleanUSN
					body.MobileNumber = cleanMobile
				}
			case *dto.CreateUserRequest:
				usn = body.USN
				mobile = body.MobileNumber
				currentYear = body.CurrentYear
				department = body.Department
				normalize = func(cleanUSN, cleanMobile string) {
					body.USN = cleanUSN
					body.MobileNumber = cleanMobile
				}
			default:
				return next(c)
			}

			if err, field := common.ValidateUserInput(usn, mobile, currentYear, department); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{
					"error": err.Error(),
					"field": field,
				})
			}

			if normalize != nil {
				normalize(strings.ToUpper(strings.TrimSpace(usn)), strings.TrimSpace(mobile))
			}

			c.Set(common.USER_INPUT_VALIDATED, true)

			return next(c)
		}
	}
}
