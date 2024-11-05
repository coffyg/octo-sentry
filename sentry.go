package octosentry

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/coffyg/octo"
	"github.com/getsentry/sentry-go"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

// SentryRecoveryMiddleware replaces the default RecoveryMiddleware.
// It captures panics and reports them to Sentry with detailed context.
func SentryRecoveryMiddleware[V any]() octo.MiddlewareFunc[V] {
	return func(next octo.HandlerFunc[V]) octo.HandlerFunc[V] {
		return func(ctx *octo.Ctx[V]) {
			defer func() {
				if err := recover(); err != nil {
					// Capture the stack trace
					var pcs [32]uintptr
					n := runtime.Callers(3, pcs[:]) // Skip first 3 callers
					frames := runtime.CallersFrames(pcs[:n])

					var stackLines []string

					for {
						frame, more := frames.Next()
						stackLines = append(stackLines, fmt.Sprintf("%s\n\t%s:%d", frame.Function, frame.File, frame.Line))

						if !more {
							break
						}
					}

					zStack := zerolog.Arr()
					for _, line := range stackLines {
						zStack.Str(line)
					}

					// Prepare the error
					var wrappedErr error
					switch e := err.(type) {
					case error:
						wrappedErr = errors.WithStack(e)
					default:
						wrappedErr = errors.Errorf("%v", e)
					}

					// Check for client abort
					if errors.Is(wrappedErr, http.ErrAbortHandler) {
						octo.GetLogger().Warn().
							Str("path", ctx.Request.URL.Path).
							Str("method", ctx.Request.Method).
							Msg("[octo-panic] Client aborted request (panic recovered)")

						// sentry.CaptureMessage(fmt.Sprintf("Client aborted request: %v", wrappedErr))
						return
					}

					// Log the error with stack trace
					octo.GetLogger().Error().
						Err(wrappedErr).
						Stack().
						Array("stack_array", zStack).
						Str("path", ctx.Request.URL.Path).
						Str("method", ctx.Request.Method).
						Msg("[octo-panic] Panic recovered")

					// Capture the panic with Sentry, including the request
					hub := sentry.CurrentHub().Clone()
					hub.Scope().SetRequest(ctx.Request)
					// Optionally set additional context, such as user info
					// hub.Scope().SetUser(sentry.User{
					//     ID: "user-id",
					// })
					hub.Recover(err)
					hub.Flush(2 * time.Second)

					// Optionally, send an HTTP 500 response
					if !strings.Contains(ctx.ResponseWriter.Header().Get("Content-Type"), "application/json") {
						http.Error(ctx.ResponseWriter, "Internal Server Error", http.StatusInternalServerError)
					} else {
						// Send a JSON error response
						ctx.SendError("err_internal_error", wrappedErr)
					}
				}
			}()

			// Proceed to the next handler
			next(ctx)
		}
	}
}
