# Introduction
This repo contains the server side of the Internet of Chuffs, written in Golang.

# Linux Set Up
I spun-up a Linux server (Ubuntu) on [Digital Ocean](https://www.digitalocean.com).  This arrives bare with root login so you will need to do some basic configuration first.

It is best to use PuTTY or some other SSH terminal as your interface to the machine as [Digital Ocean](https://www.digitalocean.com) doesn't allow copy-paste in its VNC-based terminal window.

## Set Up A User
Next, set up an admin user as follows:

`adduser username`

...where `username` is replaced by the user you wish to add.

Add this user to the sudo group with:

`usermod -aG sudo username`

Verify that you can become root with:

`su -`

Now disable root login by editing `/etc/ssh/sshd_config` to set the line `PermitRootLogin yes` to `PermitRootLogin no` and initiate this new state of affairs with:

`sudo systemctl restart ssh`

Then close your SSH terminal and start a new one with the new `username`.

## Development
You will need to develop on this machine in Golang and JS for which you must install the necessary tools:

```
sudo apt-get update
sudo apt-get install build-essential
sudo apt-get install npm
```

## DNS Entry
To avoid having to remember the IP address of the machine I added a DDNS entry for it in my account at www.noip.com.  Then I installed their Dynamic Update Client with:

```
mkdir noip
cd noip
wget http://www.no-ip.com/client/linux/noip-duc-linux.tar.gz
tar xf noip-duc-linux.tar.gz
cd noip-2.1.9-1/
sudo make install
```
You will need to supply your www.noip.com account details and chose the correct DDNS entry to link to the server machine.

Set permissions correctly with:

```
sudo chmod 700 /usr/local/bin/noip2
sudo chown root:root /usr/local/bin/noip2
sudo chmod 600 /usr/local/etc/no-ip2.conf
sudo chown root:root /usr/local/etc/no-ip2.conf
```
Create a file named `noip.service` in the `/etc/systemd/system/` directory with the following contents:

```
[Unit]
Description=No-ip.com dynamic IP address updater
After=network-online.target
After=syslog.target

[Install]
WantedBy=multi-user.target
Alias=noip.service

[Service]
# Start main service
ExecStart=/usr/local/bin/noip2
Restart=always
Type=forking
```
Check that the `noip` daemon starts correctly with:

`sudo systemctl start noip`

Your www.noip.com account should show that the update client has been in contact.  Reboot and check that the service has been started automatically with:

`sudo systemctl status noip`

...and by checking once more that your www.noip.com account shows that the update client has been in contact.

## SSHD Configuration
When setting up SSH connections that listen at the server end (i.e. with the `-R` option) there is a problem where, if the connection is dropped without notice, any subsequent attempt to re-establish the connection will fail as the port is considered to be already in use at the server end.  The only way out is to set a keep-alive with a max count from the server end, so that inactive connections are dropped.  Do this by editing `/etc/ssh/sshd_config` (note that the file is `sshd_config` and NOT `ssh_config`) to add the lines:

```
ClientAliveInterval 30
ClientAliveCountMax 2
```
...then restart the `ssh` daemon with `sudo systemctl restart sshd`

## File Transfer
I also chose to install VSFTP:

`sudo apt-get install vsftpd`

Get permissions correct with:

`sudo chown root:root /etc/vsftpd.conf`

Make sure that `/etc/vsftpd.conf` includes the line:

`listen=YES`

...and restart the service:

`sudo systemctl restart vsftpd`

To check that `vsftpd` started successfully, enter:

`sudo systemctl status vstfp`

If it failed with status code 2, try editing `etc/vsftpd.conf` and comment out the line `listen_ipv6=YES` by putting a `#` before it.  I had to do this, no idea why.

Check that you can get a response from the ftp server by entering:

`ftp 127.0.0.1`

You should get something like:

```
Connected to 127.0.0.1.
220 (vsFTPd 3.0.3)
Name (127.0.0.1:username):
```
Type "quit" and press enter to leave ftp.

Then edit `/etc/vsftpd.conf` to disable anonymous FTP (`anonymous_enable=NO`), allow local users to log in (`local_enable=YES`) and enable writing (`write_enable=YES`).   Restart the vsfptd service once more and check that you can log into the FTP server from somewhere else as the user `username`.

Set vsftpd to start at boot by entering:

`sudo systemctl enable vsftpd`

# ioc-server Application
## Installation
Install `golang` and `git `with:

```
sudo apt-get install golang-go
sudo apt-get install git
```
Edit `/etc/profile` and add to it the following lines:

```
export PATH=$PATH:/usr/local/go/bin
export GOPATH="/home/username/gocode"
```
...changing `username` to match your user name on the system.

To protect the server from unauthorised users, make sure you have generated and installed key pairs according to the [instructions for the ioc-client](https://github.com/RobMeades/ioc-client).  If you are also ready to SSH in based on certificates rather then user name and password, edit the file `/etc/ssh/sshd_config` and set `PasswordAuthentication` to `no`, then restart the `ssh` daemon with `sudo systemctl restart sshd`; the [Digital Ocean](https://www.digitalocean.com) machines are under constant brute-force attack, usually from IP addresses originating in China, so it is advisable to use certificates.

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

Grab the `ioc-server` code and build it with:

`go get github.com/RobMeades/ioc-server`

This will fail as the `lame.h` header file is not in the right place; copy it from the `libmp3lame` directory to the right place with:

```
mkdir ~/gocode/src/github.com/RobMeades/ioc-server/lame/lame
cp ~/libmp3lame/include/lame.h ~/gocode/src/github.com/RobMeades/ioc-server/lame/lame/
```
...then run:

`go get -u github.com/RobMeades/ioc-server`

## Sample HTML Files
Some simple sample HTML files are included in the `html` directory of this repo.  Copy these files to your chosen live playlists directory (e.g. `~/chuffs/live` in the example usage below) so that the `ioc-server` can serve them to the user.  These files are tested to work on Chrome, Firefox and Safari desktop and mobile browsers.

## Usage
To run the code, do something like:

`~/gocode/bin/ioc-server 1234 5678 ~/chuffs/live/chuffs -p 7 -o 300 -r ~/chuffs/audio.pcm -l ~/chuffs/ioc-server.log`

...where:

- `1234` is the port number that `ioc-server` should receive packets on,
- `5678` is the port number on which the `ioc-server` should listen for HTTP connections,
- `~/chuffs/live/chuffs` is the path to the live playlists file that the `ioc-server` will create (i.e. in this case `chuffs.m3u8` in the `~/chuffs/live` directory),
- `-s` the duration of each HLS segment file in milliseconds (defaults to 1000),
- `-p` indicates the maximum length of the HLS playlist in seconds (defaults to 7),
- `-o` the number of seconds of inactivity after which to assume that we are out of service and reset the stream (defaults to 300),
- `-r ~/chuffs/audio.pcm` is the (optional) raw 16-bit PCM output file,
- `-l ~/chuffs/ioc-server.log` will contain the (optional) file for log output from `ioc-server`.

## Boot Setup
To run the `ioc-server` at boot, create a file called something like `/lib/systemd/system/ioc-server.service` with contents something like:

```
[Unit]
Description=IoC server
After=network-online.target

[Service]
ExecStart=/home/username/gocode/bin/ioc-server 1234 5678 /home/username/chuffs/live/chuffs
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```
...where `username` is replaced by you user name on the system, etc.  Note that the `-r` and `-l` options are left out as they could eat your hard disk.

Test this with:

`sudo systemctl start ioc-server`

...using `sudo systemctl status ioc-server` to check that it looks OK and then actually running an end-to-end link with the [ioc-client](https://github.com/RobMeades/ioc-client).  If all looks good, set it to run at boot with:

`sudo systemctl enable ioc-server`

Reboot and check that it starts correctly; if it does not, check what happened with `sudo journalctl -b` and/or `sudo dmesg`.

# HLS
It is possible to use [hls.js](https://github.com/video-dev/hls.js) from a content delivery network, e.g. https://cdn.jsdelivr.net/npm/hls.js@latest.  However, I thought that [debugging and tweaking may be required](https://github.com/video-dev/hls.js/blob/master/docs/API.md) for the real-timeness and cellular-flakiness of this application and hence I installed it on the server so that it could be served directly, in modified form if required.  Install/build it with:

```
git clone https://github.com/video-dev/hls.js.git
cd hls.js
npm install
```

# LHLS
Tomo at Openfresh, a live streaming service, has [modified HLS]( https://github.com/openfresh/hls.js) to add low-latency capability; the mod is described [here](https://medium.com/freshdevelopers/implementing-lhls-on-hls-js-4fc4558edff2). `ioc-server` does not use the `#EXT-X-FRESH-IS-COMING` tag, so I don't know if it is having any beneficial effect however, since this is going to be merged into [hls.js](https://github.com/video-dev/hls.js), I decided to use it in order to keep up with the game.

# Credits
This repo includes code imported from:

https://github.com/viert/lame

Copyright, and my sincere thanks, remains with the original author(s).