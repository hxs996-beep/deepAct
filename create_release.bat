@echo off
set TOKEN=%GITHUB_TOKEN%
curl -s --connect-timeout 10 --max-time 15 -H "Authorization: Bearer %TOKEN%" -H "Accept: application/vnd.github+json" -H "Content-Type: application/json" -d @release.json https://api.github.com/repos/hxs996-beep/deepAct/releases
