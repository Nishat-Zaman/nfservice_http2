# nfservice_http2

http1.1-

go run nf1.go -version 1
go run nf2.go -version 1
curl -X GET http://localhost:8060/nf2loc

http2-

go run nf1.go -version 2
go run nf2.go -version 2
curl -X GET https://localhost:8060/nf2loc -k
