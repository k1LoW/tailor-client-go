default: test

ci: depsdev test

test:
	go test ./... -coverprofile=coverage.out -covermode=count -count=1

lint:
	golangci-lint run ./...
	go vet -vettool=`which gostyle` -gostyle.config=$(PWD)/.gostyle.yml ./...

depsdev:
	go install github.com/k1LoW/gostyle@latest

prerelease_for_tagpr:
	git add CHANGELOG.md go.mod go.sum

.PHONY: default ci test lint depsdev prerelease_for_tagpr
