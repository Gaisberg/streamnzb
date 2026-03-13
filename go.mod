module streamnzb

go 1.25.6

require (
	github.com/MunifTanjim/go-ptt v0.14.1
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/javi11/rapidyenc v0.0.0-20260215144528-f0dac5a39d34
	github.com/javi11/rardecode/v2 v2.1.2-0.20260213142800-2b1c601a8d62
	github.com/joho/godotenv v1.5.1
	github.com/klauspost/compress v1.18.4 // indirect
	github.com/pierrec/lz4/v4 v4.1.25 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	go4.org v0.0.0-20260112195520-a5071408f32f // indirect
	golang.org/x/text v0.34.0
)

require golang.org/x/net v0.50.0

require (
	github.com/javi11/sevenzip v1.6.2-0.20251026160715-ca961b7f1239
	golang.org/x/sync v0.19.0
)

replace github.com/javi11/rardecode/v2 => ./third_party/rardecode
