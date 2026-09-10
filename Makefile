ensure:
	cd application && \
	go mod tidy && \
	go mod download

test:
	cd application && \
	go test ./...

buildapp:
	cd application && \
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -o bootstrap main.go && \
	zip deployment.zip bootstrap && \
	rm bootstrap && \
	mv deployment.zip ../
	unzip -Z1 deployment.zip | grep -qx bootstrap
	unzip -Z deployment.zip bootstrap | grep -q '^-rwx'
	unzip -p deployment.zip bootstrap | od -An -tx1 -N20 | tr -d ' \n' | grep -q '^7f454c46020101.*3e00'
