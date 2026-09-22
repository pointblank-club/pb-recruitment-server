# PB Recruitment Server

A server-side application for managing recruitment workflows and processes for the PointBlank Club.

## Features

- Candidate application management
- Review and evaluation tools
- User/admin authentication
- RESTful API for integrations

## Prerequisites

- [Node.js](https://nodejs.org/) >= 16.x
- [npm](https://www.npmjs.com/) or [yarn](https://yarnpkg.com/)

## Getting Started

Clone the repository:

```bash
git clone https://github.com/pointblank-club/pb-recruitment-server.git
cd pb-recruitment-server
```

Install dependencies:

```bash
npm install
# or
yarn install
```

Configure environment variables:

1. Copy `.env.example` to `.env`
2. Update values as needed

Start the development server:

```bash
npm run dev
# or
yarn dev
```

# How to run?
- Install [air](https://github.com/air-verse/air?tab=readme-ov-file#installation)
- Run `air` command in the terminal
- Server is running on `http://localhost:8080`

## DB Migrations

- Install [migrate](https://github.com/golang-migrate/migrate)
- Add `DB_ADDR` to `.env`

### Migration Commands

```bash
# Create a new migration
make migration create_users_table

# Run migrations up
make migrate-up

# NOT AVAILABLE ON PROD - Rollback migrations (specify number of steps)
make migrate-down 1

# NOT AVAILABLE ON PROD - Force migration version (use with caution)
make migrate-force 1
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

Distributed under the Apache 2.0 License. See [`LICENSE`](LICENSE) for more information.

---

Maintained by the [PointBlank Club](https://github.com/pointblank-club)
