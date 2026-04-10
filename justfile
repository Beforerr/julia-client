install:
    go build -o ~/.local/bin/julia-client ./go/

test:
    go test -v -timeout 300s ./go/

build:
    go build -o julia-client ./go/
