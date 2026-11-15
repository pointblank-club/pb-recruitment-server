package middleware

import "github.com/labstack/echo/v4"

func PrivateNoStore(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		c.Response().Header().Set(echo.HeaderCacheControl, "private, no-store")
		return next(c)
	}
}
