## Install
libgit2@1.3.0 needs to be installed. On mac use macports since brew doesn't seem
to have it in official repos at the time of writing this.

```sh
sudo port install libgit2@1.3.0
go install github.com/nrawrx3/changelog
```

## Examples

*Commits can be denoted as hashes or refs. Also make sure to pull and rebase changes
from origin before running command to avoid any confusion.*

### From branch `master` to branch `develop`

`changelog -out changelog.md -config config.json -start refs/heads/master -end refs/heads/develop`

### From one commit to another

`changelog -out changelog.md -config config.json -start f5a78eba828b905cfb559a427e1afcceb5d337ca -end 9fda7b8c7c77b03f630973d4373d946adfaa76f7`

### From a commit to a reference (that points to the head of a branch)

`changelog -out changelog.md -config config.json -start f5a78eba828b905cfb559a427e1afcceb5d337ca -end refs/heads/develop -out changelog.md`

### From another branch to current head

`changelog -out changelog.md -config config.json -start refs/heads/master -end HEAD -out changelog.md`

### From one remote head to another remote head

*Probably better to not do this to avoid confusion*

`changelog -out changelog.md -config config.json -start refs/remotes/origin/master -end refs/remotes/origin/develop`
