FROM node:22 AS dashboard
WORKDIR /src/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci
COPY dashboard/ ./
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/pgmux ./cmd/pgmux

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pgmux /pgmux
COPY --from=dashboard /src/dashboard/dist /usr/share/pgmux/dashboard
EXPOSE 6432 9930
ENTRYPOINT ["/pgmux"]
CMD ["-config", "/etc/pgmux/pgmux.yaml"]
