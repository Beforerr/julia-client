install:
    go build -o ~/.local/bin/julia-client ./go/
    npx skills add . -g -y

test:
    go test -v -timeout 300s ./go/

build:
    go build -o julia-client ./go/

release version="":
    #!/usr/bin/env bash
    set -euo pipefail
    if [[ -z "{{ version }}" ]]; then
        latest=$(jj log --no-graph -r 'tags()' --template 'tags ++ "\n"' 2>/dev/null | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)
        if [[ -z "$latest" ]]; then
            echo "No existing semver tag found. Please provide a version explicitly: just release v0.1.0" >&2
            exit 1
        fi
        IFS='.' read -r major minor patch <<< "${latest#v}"
        version="v${major}.$((minor + 1)).0"
        echo "Bumping minor: $latest -> $version"
    fi
    jj tag set "$version" --revision @-
    git push --tags
