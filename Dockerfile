from golang:1.18.1

workdir /app

# install dependecies
COPY go.mod go.sum ./
RUN go mod download

# copy sause
COPY *.go *.json ./
COPY decap/ ./decap/
COPY templates/ ./templates/

# build
RUN go build

RUN mv spectura /bin/

CMD ["spectura"]
