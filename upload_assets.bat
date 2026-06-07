@echo off
set TOKEN=%GITHUB_TOKEN%
set UPLOAD_URL=https://uploads.github.com/repos/hxs996-beep/deepAct/releases/335424488/assets

echo Uploading windows/amd64...
curl -s --connect-timeout 15 --max-time 60 -H "Authorization: Bearer %TOKEN%" -H "Content-Type: application/octet-stream" --data-binary @build\deepact_windows_amd64\deepact.exe "%UPLOAD_URL%?name=deepact_windows_amd64.exe"

echo Uploading linux/amd64...
curl -s --connect-timeout 15 --max-time 60 -H "Authorization: Bearer %TOKEN%" -H "Content-Type: application/octet-stream" --data-binary @build\deepact_linux_amd64\deepact "%UPLOAD_URL%?name=deepact_linux_amd64"

echo Uploading linux/arm64...
curl -s --connect-timeout 15 --max-time 60 -H "Authorization: Bearer %TOKEN%" -H "Content-Type: application/octet-stream" --data-binary @build\deepact_linux_arm64\deepact "%UPLOAD_URL%?name=deepact_linux_arm64"

echo Uploading darwin/amd64...
curl -s --connect-timeout 15 --max-time 60 -H "Authorization: Bearer %TOKEN%" -H "Content-Type: application/octet-stream" --data-binary @build\deepact_darwin_amd64\deepact "%UPLOAD_URL%?name=deepact_darwin_amd64"

echo Uploading darwin/arm64...
curl -s --connect-timeout 15 --max-time 60 -H "Authorization: Bearer %TOKEN%" -H "Content-Type: application/octet-stream" --data-binary @build\deepact_darwin_arm64\deepact "%UPLOAD_URL%?name=deepact_darwin_arm64"

echo All uploads complete
