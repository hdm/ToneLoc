module github.com/hdm/toneloc

go 1.25.0

require (
	github.com/hdm/zmap-go v0.0.0-00010101000000-000000000000
	golang.org/x/net v0.53.0
	golang.org/x/term v0.42.0
)

require golang.org/x/sys v0.43.0 // indirect

replace github.com/hdm/zmap-go => ../zmap-go
