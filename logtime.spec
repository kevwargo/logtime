Name:           logtime
Version:        0.2.0
Release:        1%{?dist}
Summary:        Execute a command and log its output with timestamps

License:        MIT
URL:            https://github.com/kevwargo/logtime

BuildRequires:  golang

%description
A simple utility that executes an arbitrary command and logs its stdout and
stderr line by line, prepending a human-readable timestamp to each line.

%prep
# Nothing to do - building from the git worktree directly

%build
cd %{_sourcedir}
go build -o logtime .

%install
install -D -m 0755 %{_sourcedir}/logtime %{buildroot}%{_bindir}/logtime

%files
%{_bindir}/logtime
