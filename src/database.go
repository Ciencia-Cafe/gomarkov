package main

import (
	"context"
	"os"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var MongodbClient *mongo.Client
var MongodbDatabase *mongo.Database
var MessageCollection *mongo.Collection

type Message struct {
	Id        primitive.ObjectID `bson:"_id,omitempty"`
	CreatedAt primitive.DateTime `bson:"CreatedAt"`
	AuthorId  string             `bson:"AuthorId"`
	Content   string             `bson:"Content"`
}

func InitDatabase() bool {
	username := os.Getenv("MONGODB_USERNAME")
	password := os.Getenv("MONGODB_PASSWORD")
	connectionString := os.Getenv("MONGODB_CONNECTION_STRING")

	if username == "" || password == "" || connectionString == "" {
		Error("missing credentials to connect to mongodb cluster in .env file")
		return false
	}

	uri := connectionString
	uri = strings.Replace(uri, "<db_username>", username, 1)
	uri = strings.Replace(uri, "<db_password>", password, 1)

	serverApi := options.ServerAPI(options.ServerAPIVersion1)
	opts := options.Client().ApplyURI(uri).SetServerAPIOptions(serverApi)

	var err error
	MongodbClient, err = mongo.Connect(context.Background(), opts)
	if err != nil {
		Error("error when connecting to mongodb cluster: ", err)
		return false
	}

	MongodbDatabase = MongodbClient.Database("production")
	if MongodbDatabase == nil {
		Error("failed to find database")
		return false
	}

	MessageCollection = MongodbDatabase.Collection("messages")
	if MessageCollection == nil {
		Error("failed to get messages collection")
		return false
	}

	return true
}

func DoneDatabase() bool {
	if err := MongodbClient.Disconnect(context.Background()); err != nil {
		Warn("error when disconnecting to mongodb cluster: ", err)
		return false
	}
	return true
}

func ConsumeCursorToChannel[T any](cursor *mongo.Cursor, ch chan []T) {
	ctx := context.TODO()

	for {
		batchSize := cursor.RemainingBatchLength()
		batch := make([]T, batchSize)

		if batchSize <= 0 {
			break
		}
		for i := range batchSize {
			cursor.Decode(&batch[i])
			cursor.TryNext(ctx)
		}

		// Info("sending batch of length", len(batch), "to channel")
		ch <- batch

		if !cursor.Next(ctx) {
			break
		}
	}

	cursor.Close(ctx)
	close(ch)
}
