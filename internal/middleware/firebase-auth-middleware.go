package middleware

import (
	"app/internal/common"
	"context"
	"net/http"
	"strings"

	"firebase.google.com/go/v4/auth"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
)

// FirebaseAuth is a middleware that validates Firebase ID tokens
// If optional is true, requests without tokens will be allowed through
func FirebaseAuth(authClient *auth.Client, optional bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Get token from Authorization header
			authHeader := c.Request().Header.Get("Authorization")
			idToken := ""

			// Check if Authorization header exists and has the Bearer prefix
			if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
				idToken = strings.TrimPrefix(authHeader, "Bearer ")
			}

			// // Bypass for testing
			// if idToken == "!!TESTING!!" {
			// 	c.Set(common.AUTH_USER_ID, "test")
			// 	return next(c)
			// }

			// If authentication is optional and no token is provided, skip verification, otherwise return an error
			if idToken == "" {
				if optional {
					return next(c)
				}

				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "missing or malformed ID token",
				})
			}

			// Verify the ID token using the Firebase Auth client
			token, err := authClient.VerifyIDToken(context.Background(), idToken)
			if err != nil {
				log.Errorf("Failed to verify ID token: %v", err)
				return c.JSON(http.StatusUnauthorized, map[string]string{
					"error": "invalid ID token",
				})
			}

			// Add the verified user information to the context
			c.Set(common.AUTH_USER_ID, token.UID)

			// Enrich Sentry hub scope with user identity
			if hub := sentryecho.GetHubFromContext(c); hub != nil {
				hub.ConfigureScope(func(scope *sentry.Scope) {
					sentryUser := sentry.User{
						ID: token.UID,
					}
					if email, ok := token.Claims["email"].(string); ok && email != "" {
						sentryUser.Email = email
					}
					scope.SetUser(sentryUser)
				})
			}

			// Call the next handler
			return next(c)
		}
	}
}

func RequireFirebaseAuth(authClient *auth.Client) echo.MiddlewareFunc {
	return FirebaseAuth(authClient, false)
}

func OptionalFirebaseAuth(authClient *auth.Client) echo.MiddlewareFunc {
	return FirebaseAuth(authClient, true)
}
