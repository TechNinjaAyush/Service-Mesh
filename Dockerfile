
FROM golang:1.25  


WORKDIR /app  

COPY go.mod go.sum ./  

RUN go mod download  

COPY . .  

RUN CGO_ENABLED=0 GOOS=linux go build -o service-mesh ./cmd/server


EXPOSE 8080  

CMD ["/app/service-mesh"]
