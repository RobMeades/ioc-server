Introduction
============
This repo contains the server side of the Internet of Chuffs, written in Golang.

Installation (on Linux only I'm afraid)
=======================================
Install `golang` and `git `with:

`sudo apt-get install golang-go`
`sudo apt-get install git`

Edit `/etc/profile` and add to it the following line:

`export PATH=$PATH:/usr/local/go/bin`

Add the `GOPATH` environment variable to your path.  For me, with a home directory `rob`, the path would be:

`export GOPATH="/home/rob/gocode"`

Install SSH with:

`sudo-apt-get install openssh-server`

...and make sure that you can log in using SSH from another machine with your username/password.

To protect the server from unauthorised users, make sure you have generated and installed key pairs according to the [instructions for the ioc-client](https://github.com/RobMeades/ioc-client), then edit the file `/etc/ssh/sshd_config` and set `PasswordAuthentication` to `no`, then restart the `ssd` daemon with `sudo systemctl restart sshd`.

Make sure that you have Lame installed, with something like:

`sudo apt-get install lame`

You will then need to create a symlink to the library versions it has installed.  For instance, if the installed Lame library was:

`/usr/lib/x86_64-linux-gnu/libmp3lame.so.0`

...then you would create the symlink `libmp3lame.so` as follows:

`sudo ln -s /usr/lib/x86_64-linux-gnu/libmp3lame.so.0 /usr/lib/x86_64-linux-gnu/libmp3lame.so`

What you won't have is the `lame.h` header file.  Get all of the lame source code with:

`git clone https://github.com/gypified/libmp3lame`

Find out where the `lame.h` header file has ended up with:

`sudo find / -name lame.h`

Grab the code and build it with:

`go get github.com/RobMeades/ioc-server`

This will fail as the `lame.h` header file is not in the right place copy it from the `libmp3lame` directory to the right place with:

`mkdir ~/gocode/src/github.com/RobMeades/ioc-server/lame/lame`
`cp ~/libmp3lame/include/lame.h ~/gocode/src/github.com/RobMeades/ioc-server/lame/lame/`

Usage
=====
To run the code, do something like:

`./ioc-server 5060 8080 ~/chuffs/live/chuffs -c -t -o ~/chuffs/oos -r ~/chuffs/audio.pcm -l ~/chuffs/ioc-server.log`

...where:

- `5060` is the port number that `ioc-server` should receive packets on,
- `8080` is the port number on which the `ioc-server` should listen for HTTP connections,
- `~/chuffs/live/chuffs` is the path to the live playlists file that the `ioc-server` will create (i.e. in this case `chuffs.m3u8` in the `~/chuffs/live` directory),
- `-c` indicates that old segments files should be deleted from the live playlists directory at start-up,
- `-t` indicates that a TCP connection is expected (otherwise UDP packets),
- `-o ~/chuffs/oos` optionally gives the directory containing HTML and, if required, in the same directory, static playlist/audio files, to use when there is no live audio to stream (you must create these files yourself),
- `-r ~/chuffs/audio.pcm` is the (optional) raw 16-bit PCM output file,
- `-l ~/chuffs/ioc-server.log` will contain the (optional) file for log output from `ioc-server`.

Credits
=======
This repo includes code imported from:

https://github.com/viert/lame

Copyright, and our sincere thanks, remains with the original author(s).