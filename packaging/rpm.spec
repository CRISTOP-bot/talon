Name:           talon
Version:        %{_version}
Release:        1%{?dist}
Summary:        Agentic coding CLI for the terminal
License:        MIT
URL:            https://github.com/talon-cli/talon
BuildArch:      x86_64

%description
Talon reads your project, plans changes, edits files, runs your build and tests,
reads the errors and fixes the cause, all from a single prompt.

%prep

%build

%install
mkdir -p %{buildroot}%{_bindir}
cp %{_sourcedir}/talon %{buildroot}%{_bindir}/talon

%files
%{_bindir}/talon

%changelog
* Mon Jan 01 2026 Talon contributors <maintainers@talon.dev> - %{_version}-1
- Automated build; see CHANGELOG.md
