buildapp:
	GOOS=linux GOARCH=amd64 go build -o main application/main.go
	rm deployment.zip
	zip deployment.zip main
	rm main
