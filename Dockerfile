FROM golang:1.19 as build

WORKDIR /go/src/director
COPY . .

RUN go mod download
RUN go vet -v
RUN go test -v

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /go/bin/director

FROM golang:1.19
COPY --from=build /go/bin/director /

ENV GIN_MODE=release
CMD ["/director"]