package model

type Route string

const (
	RouteTelegram  Route = "telegram"
	RouteProjects  Route = "projects"
	RouteDashboard Route = "dashboard"
	RouteBuckets   Route = "buckets"
	RouteKeys      Route = "keys"
	RouteObjects   Route = "objects"
	RouteReplica   Route = "replica"
	RouteSettings  Route = "settings"
)

type Params map[string]string

const (
	ParamBucket = "bucket"
	ParamPrefix = "prefix"
	ParamStep   = "step"
)

type Clipboard interface {
	SetText(text string)
	Text() string
}
