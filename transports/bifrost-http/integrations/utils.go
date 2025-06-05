package integrations

import "github.com/fasthttp/router"

type ExtensionRouter interface {
	RegisterRoutes(r *router.Router)
}
