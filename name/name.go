package name

import (
	"github.com/gin-gonic/gin"
)

var CONTEXT_TYPE = "GIN.ENGINE.NAME.TYPE"
var CONTEXT_ACTION = "GIN.ENGINE.NAME.ACTION"

func Middleware(typ, action string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.Set(CONTEXT_TYPE, typ)
		ctx.Set(CONTEXT_ACTION, action)
		ctx.Next()
	}
}
