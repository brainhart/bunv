release:
	#!/bin/bash
	export GITHUB_TOKEN=$(op --account NERGFRMYDJDY7LFSDRPG3A5YL4 read "op://Private/p7un63xsy5av74j57n3dsnoirm/password")
	goreleaser release
