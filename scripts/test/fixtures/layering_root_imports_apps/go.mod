module github.com/PRIEYAN/ButterDB

go 1.26.0

require (
	example.com/thirdparty v0.0.0
	github.com/gotd/td v0.0.0
	modernc.org/sqlite v0.0.0
)

replace example.com/thirdparty => ../stubs/thirdparty

replace github.com/gotd/td => ../stubs/gotd

replace modernc.org/sqlite => ../stubs/sqlite
