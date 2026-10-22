default:
    @just --list

build:
    @echo '{{ BOLD + CYAN }}Building sms2telegram{{ NORMAL }}'
    go build -o sms2telegram ./cmd/sms2telegram

update:
    @cd ./cmd/sms2telegram && go get -u

test:
    go test -race -v ./... -covermode=atomic -coverprofile=coverage.out

lint:
    golangci-lint run -c .golangci.yml

tidy:
    go mod tidy
