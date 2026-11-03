package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Environment struct {
	AdminDSN string
	Root     string
	CLI      string
	Pool     int
}

func (environment Environment) databaseDSN(name string) (string, error) {
	parsed, err := url.Parse(environment.AdminDSN)
	if err != nil {
		return "", fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	parsed.Path = "/" + name
	return parsed.String(), nil
}

type Database struct {
	Name    string
	DSN     string
	drop    func() error
	dropped bool
}

func (database *Database) Drop() error {
	if database.dropped {
		return nil
	}
	database.dropped = true
	return database.drop()
}

func (environment Environment) CreateDatabase(ctx context.Context) (*Database, error) {
	admin, err := sql.Open("pgx", environment.AdminDSN)
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close() }()
	name := fmt.Sprintf("loadmix_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		return nil, fmt.Errorf("create database: %w", err)
	}
	dsn, err := environment.databaseDSN(name)
	if err != nil {
		return nil, err
	}
	database := &Database{Name: name, DSN: dsn}
	database.drop = func() error {
		cleanup, err := sql.Open("pgx", environment.AdminDSN)
		if err != nil {
			return err
		}
		defer func() { _ = cleanup.Close() }()
		_, err = cleanup.ExecContext(context.WithoutCancel(ctx), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
		return err
	}
	command := exec.CommandContext(ctx, environment.CLI, "migration", "apply", "--provider", "postgresql", "--dsn", dsn, "--migrations", "migrations")
	command.Dir = environment.Root
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		return nil, errors.Join(fmt.Errorf("apply reviewed migration: %w\n%s", err, output), database.Drop())
	}
	return database, nil
}
