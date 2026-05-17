package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Success is the envelope returned for every successful response.
type Success struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
}

// ErrorDetail carries a machine-readable code and a human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Failure is the envelope returned whenever a request cannot be fulfilled.
type Failure struct {
	Success bool        `json:"success"`
	Error   ErrorDetail `json:"error"`
}

// OK writes a 200 JSON response wrapped in the success envelope.
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Success{Success: true, Data: data})
}

// Fail writes a JSON error response with the given HTTP status code.
func Fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, Failure{Success: false, Error: ErrorDetail{Code: code, Message: message}})
}

// ValidationFail writes a 422 JSON response for request-body validation errors.
func ValidationFail(c *gin.Context, message string) {
	Fail(c, http.StatusUnprocessableEntity, "VALIDATION_ERROR", message)
}
