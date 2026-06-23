module github.com/Gerry3010/projecthub

go 1.26.3

require (
	github.com/Gerry3010/passbubble/backend v0.0.0
	github.com/go-chi/chi/v5 v5.3.0
	github.com/google/uuid v1.6.0
	github.com/maxence-charriere/go-app/v10 v10.1.11
)

require (
	github.com/cloudflare/circl v1.6.3 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/Gerry3010/passbubble/backend => ../Password-Manager/backend
