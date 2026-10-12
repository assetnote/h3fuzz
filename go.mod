module github.com/assetnote/h3fuzz

go 1.26.0

require (
	github.com/PuerkitoBio/goquery v1.13.0
	github.com/adrg/xdg v0.5.3
	github.com/andybalholm/brotli v1.2.4
	github.com/coder/websocket v1.8.15
	github.com/ffuf/pencode v0.0.0-20240120104927-07e33ef17d5a
	github.com/pelletier/go-toml v1.9.5
	github.com/quic-go/quic-go v0.63.0
	modernc.org/sqlite v1.60.1
)

replace github.com/quic-go/quic-go => ./third_party/quic-go

require (
	github.com/andybalholm/cascadia v1.3.5 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
