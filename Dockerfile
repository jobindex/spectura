from golang:1.18.1

workdir /app

# install dependecies
COPY go.mod go.sum ./
RUN go mod download

# copy sause
COPY *.go ./
COPY decap/ ./decap/

# build
RUN go build

RUN mv spectura /bin/

CMD ["spectura"]
