## Gembuster - Enumerating the Small Web
I'm a big fan of the {dir,go,ferox}buster tools. I love them. They work a treat. And for all its faults, I'm also interested in the Gemini protocol. This is a directory/vhost busting tool for enumerating Gemini servers written in **Go**. It has the basic functionality of other *buster tools, with plans to add filters in the near future. The syntax and usage is near-identical to other tools.

Both subdirectory and vhost/subdomain enumeration are supported.

```sh
# Install
go install github.com/boxlegs/gembuster@latest

# Basic Usage
gembuster [dir | vhost] -u geminiserver.com -w raft-common-directories-blah.txt
```

