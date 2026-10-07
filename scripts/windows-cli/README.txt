Multica CLI for TuringOmni - Windows offline installer

1. Extract the complete ZIP. Choose amd64 for Intel/AMD x64 Windows,
   or arm64 for Windows on ARM.
2. Double-click install.cmd. No administrator privileges are required.
   The script checks the binary SHA-256, installs to your user directory,
   and creates an isolated "turingomni" profile.
3. Open a new terminal and run:
   multica --profile turingomni login
   multica --profile turingomni daemon start

Service: https://workers.turingomni.com
Releases: https://github.com/dunzic/multica-worker/releases

For optional browser login during installation:
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Login

Existing default/private profiles are not overwritten. If an existing
"turingomni" profile targets a different service, installation stops.
The program name remains multica (multica.exe). Use --profile turingomni for
this service; bare multica respects your default profile and any explicit
server override. On a fresh installation, multica login uses the built-in
TuringOmni defaults. An existing profile is never silently redirected.

The binary is not Authenticode-signed. Windows may show a security warning.
Only install packages received from a trusted source. SHA-256 detects
corruption, not an untrusted distributor replacing both files.

Built on Multica. Original license and attribution are in LICENSE and NOTICE.
