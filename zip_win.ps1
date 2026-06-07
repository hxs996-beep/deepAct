$src = "D:\java_project\deepAct\build\deepact_windows_amd64\*"
$dst = "D:\java_project\deepAct\dist\deepact_v0.1.3_windows_amd64.zip"
Compress-Archive -Path $src -DestinationPath $dst -Force
if (Test-Path $dst) {
    $size = (Get-Item $dst).Length
    Write-Host "OK: $dst ($size bytes)"
} else {
    Write-Error "Failed to create zip"
}
