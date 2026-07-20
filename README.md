build server
go build -ldflags="-s -w" -o library-server.exe .\server\

build agent
go build -ldflags="-H windowsgui -s -w" -o deploy\agent.exe .\agent\