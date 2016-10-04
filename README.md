# MetaSSH

    MetaSSH connects to a bunch of hosts all at once, keeps connections open, lets you
    run commands on them, but more importantly manages a SSH ControlMaster UNIX domain
    socket for each connection.

    Dependencies:

    I have included a glide.yaml file which should let you get the vendorized
    dependencies for the program. You should just be able to do a 'glide install'
    to pull all the necessary dependencies into the vendor directory.

    github.com/bmizerany/perks/quantile        // Math is hard
    github.com/kr/pty                          // Portable pty open
    github.com/ogier/pflag                     // POSIX cmdline flags
    github.com/vividcortex/godaemon            // No fork() in go, so.. hax
    golang.org/x/sys/unix                      // Unixy things    
    golang.org/x/crypto/{ssh,agent,terminal}   // The REAL hero here

    GNU Readline:

    GNU/Linux: (yum install readline-devel)
    /usr/include/readline/readline.h libreadline.so.6
    /usr/include/readline/history.h  libhistory.so.6

    OSX: (brew install readline)
    /usr/local/opt/readline/include/readline.h
    /usr/local/opt/readline/include/history.h
    /usr/local/opt/readline/lib/libreadline.6.dylib
    /usr/local/opt/readline/lib/libhistory.6.dylib

    Targeting:

    MetaSSH doesn't know anything about your SSH servers, so it needs an external program
    called 'target' to generate JSON data with host information that it can parse.
