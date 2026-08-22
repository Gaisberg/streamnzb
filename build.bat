@echo off

REM pkg\server\web embeds pkg\server\web\static, which is gitignored and is
REM produced by the frontend build below. On a fresh clone it does not exist and
REM `//go:embed all:static` is a compile error, so every Go command here would
REM fail before checking anything. The real assets replace the placeholder later.
if not exist pkg\server\web\static mkdir pkg\server\web\static
if not exist pkg\server\web\static\.gitkeep type nul > pkg\server\web\static\.gitkeep

echo Formatting Go code...
go fmt ./...
if %errorlevel% neq 0 exit /b %errorlevel%

echo Vetting Go code...
go vet ./...
if %errorlevel% neq 0 exit /b %errorlevel%

echo Running Go tests...
rem ./... rather than ./pkg/...: cmd/streamnzb holds the shutdown and
rem listener-rebind tests, which would otherwise never run.
go test ./...
if %errorlevel% neq 0 exit /b %errorlevel%

echo Linting Frontend...
cd frontend
call npm run lint
if %errorlevel% neq 0 exit /b %errorlevel%

echo Testing Frontend...
call npm test
if %errorlevel% neq 0 exit /b %errorlevel%

echo Building Frontend...
call npm run build
if %errorlevel% neq 0 exit /b %errorlevel%
cd ..

echo Clearing static assets...
if exist pkg\server\web\static rmdir /s /q pkg\server\web\static
mkdir pkg\server\web\static
echo Copying new assets...
xcopy /E /I /Y frontend\dist\* pkg\server\web\static\
if %errorlevel% neq 0 exit /b %errorlevel%

echo Building Go Binary...
set SHORT_SHA=unknown
for /f "tokens=*" %%i in ('git rev-parse --short HEAD 2^>nul') do set SHORT_SHA=%%i
set RELEASE_VERSION=0.0.0
for /f "tokens=*" %%v in ('powershell -NoProfile -Command "try { $m=[regex]::Match((Get-Content .release-please-manifest.json -Raw), \"[0-9]+\.[0-9]+\.[0-9]+\"); if($m.Success){$m.Value}else{\"0.0.0\"} } catch { \"0.0.0\" }"') do set RELEASE_VERSION=%%v
go build -ldflags="-X main.Version=%RELEASE_VERSION%-%SHORT_SHA%" ./cmd/streamnzb/
if %errorlevel% neq 0 exit /b %errorlevel%

echo Build Complete!
