install:
    julia-client stop
    go build -C go -o ~/.local/bin/julia-client .
    npx skills add . -g -y

test:
    go test -C go -v -timeout 300s

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
