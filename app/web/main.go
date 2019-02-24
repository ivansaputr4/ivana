package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"time"

	"github.com/gorilla/context"
	"github.com/julienschmidt/httprouter"
	"github.com/justinas/alice"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/subosito/gotenv"
)

// Errors

type Errors struct {
	Errors []*Error `json:"errors"`
}

type Error struct {
	Id     string `json:"id"`
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func WriteError(w http.ResponseWriter, err *Error) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(err.Status)
	json.NewEncoder(w).Encode(Errors{[]*Error{err}})
}

var (
	ErrBadRequest           = &Error{"bad_request", 400, "Bad request", "Request body is not well-formed. It must be JSON."}
	ErrNotAcceptable        = &Error{"not_acceptable", 406, "Not Acceptable", "Accept header must be set to 'application/vnd.api+json'."}
	ErrUnsupportedMediaType = &Error{"unsupported_media_type", 415, "Unsupported Media Type", "Content-Type header must be set to: 'application/vnd.api+json'."}
	ErrInternalServer       = &Error{"internal_server_error", 500, "Internal Server Error", "Something went wrong."}
)

// Response Success

type MessageSuccess struct {
	Data MessageInfo `json:"data"`
}

type MessageInfo struct {
	Message string `json:"message`
}

func WriteSuccess(w http.ResponseWriter, httpStatus int, data interface{}) {
	w.Header().Set("Content-Type", "application/vnd.api+json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(data)
}

// Middlewares

func recoverHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("panic: %+v", err)
				WriteError(w, ErrInternalServer)
			}
		}()

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}

func loggingHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		t1 := time.Now()
		next.ServeHTTP(w, r)
		t2 := time.Now()
		log.Printf("[%s] %q %v\n", r.Method, r.URL.String(), t2.Sub(t1))
	}

	return http.HandlerFunc(fn)
}

func acceptHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.api+json" {
			WriteError(w, ErrNotAcceptable)
			return
		}

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}

func contentTypeHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/vnd.api+json" {
			WriteError(w, ErrUnsupportedMediaType)
			return
		}

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}

func bodyHandler(v interface{}) func(http.Handler) http.Handler {
	t := reflect.TypeOf(v)

	m := func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			val := reflect.New(t).Interface()
			err := json.NewDecoder(r.Body).Decode(val)

			if err != nil {
				WriteError(w, ErrBadRequest)
				return
			}

			if next != nil {
				context.Set(r, "body", val)
				next.ServeHTTP(w, r)
			}
		}

		return http.HandlerFunc(fn)
	}

	return m
}

// Router

type router struct {
	*httprouter.Router
}

func (r *router) Get(path string, handler http.Handler) {
	r.GET(path, wrapHandler(handler))
}

func (r *router) Post(path string, handler http.Handler) {
	r.POST(path, wrapHandler(handler))
}

func (r *router) Patch(path string, handler http.Handler) {
	r.PATCH(path, wrapHandler(handler))
}

func (r *router) Delete(path string, handler http.Handler) {
	r.DELETE(path, wrapHandler(handler))
}

func NewRouter() *router {
	return &router{httprouter.New()}
}

func wrapHandler(h http.Handler) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		context.Set(r, "params", ps)
		h.ServeHTTP(w, r)
	}
}

// Repo Office

type Office struct {
	Id   bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name string        `json:"name"`
}

type OfficesCollection struct {
	Data []Office `json:"data"`
}

type OfficeResource struct {
	Data Office `json:"data"`
}

type OfficeRepo struct {
	coll *mgo.Collection
}

func (r *OfficeRepo) All() (OfficesCollection, error) {
	result := OfficesCollection{[]Office{}}
	err := r.coll.Find(nil).All(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *OfficeRepo) Find(id string) (OfficeResource, error) {
	result := OfficeResource{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *OfficeRepo) Create(office *Office) error {
	id := bson.NewObjectId()
	_, err := r.coll.UpsertId(id, office)
	if err != nil {
		return err
	}

	office.Id = id

	return nil
}

func (r *OfficeRepo) Update(office *Office) error {
	err := r.coll.UpdateId(office.Id, office)
	if err != nil {
		return err
	}

	return nil
}

func (r *OfficeRepo) Delete(id string) error {
	err := r.coll.RemoveId(bson.ObjectIdHex(id))
	if err != nil {
		return err
	}

	return nil
}

// Main handlers

type appContext struct {
	db *mgo.Database
}

// Office Handlers

func (c *appContext) officesHandler(w http.ResponseWriter, r *http.Request) {
	repo := OfficeRepo{c.db.C("offices")}
	offices, err := repo.All()
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, offices)
}

func (c *appContext) officeHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := OfficeRepo{c.db.C("offices")}
	office, err := repo.Find(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, office)
}

func (c *appContext) createOfficeHandler(w http.ResponseWriter, r *http.Request) {
	body := context.Get(r, "body").(*OfficeResource)
	repo := OfficeRepo{c.db.C("offices")}
	err := repo.Create(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateOfficeHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*OfficeResource)
	body.Data.Id = bson.ObjectIdHex(params.ByName("id"))
	repo := OfficeRepo{c.db.C("offices")}
	err := repo.Update(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusAccepted, body)
}

func (c *appContext) deleteOfficeHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := OfficeRepo{c.db.C("offices")}
	err := repo.Delete(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	data := MessageSuccess{MessageInfo{Message: "Office has been deleted successfully"}}
	WriteSuccess(w, http.StatusAccepted, data)
}

// Repo Room

type Room struct {
	Id       bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name     string        `json:"name"`
	OfficeId string        `json:office_id`
}

type RoomsCollection struct {
	Data []Room `json:"data"`
}

type RoomResource struct {
	Data Room `json:"data"`
}

type RoomRepo struct {
	coll *mgo.Collection
}

func (r *RoomRepo) All() (RoomsCollection, error) {
	result := RoomsCollection{[]Room{}}
	err := r.coll.Find(nil).All(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *RoomRepo) Find(id string) (RoomResource, error) {
	result := RoomResource{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *RoomRepo) Create(room *Room) error {
	id := bson.NewObjectId()
	_, err := r.coll.UpsertId(id, room)
	if err != nil {
		return err
	}

	room.Id = id

	return nil
}

func (r *RoomRepo) Update(room *Room) error {
	err := r.coll.UpdateId(room.Id, room)
	if err != nil {
		return err
	}

	return nil
}

func (r *RoomRepo) Delete(id string) error {
	err := r.coll.RemoveId(bson.ObjectIdHex(id))
	if err != nil {
		return err
	}

	return nil
}

func (r *RoomRepo) AllByOfficeId(officeId string) (RoomsCollection, error) {
	result := RoomsCollection{[]Room{}}
	err := r.coll.Find(bson.M{"office_id": officeId}).All(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

// Room Handlers

func (c *appContext) roomsHandler(w http.ResponseWriter, r *http.Request) {
	repo := RoomRepo{c.db.C("rooms")}
	rooms, err := repo.All()
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, rooms)
}

func (c *appContext) roomHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := RoomRepo{c.db.C("rooms")}
	room, err := repo.Find(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, room)
}

func (c *appContext) createRoomHandler(w http.ResponseWriter, r *http.Request) {
	body := context.Get(r, "body").(*RoomResource)
	repo := RoomRepo{c.db.C("rooms")}
	err := repo.Create(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateRoomHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*RoomResource)
	body.Data.Id = bson.ObjectIdHex(params.ByName("id"))
	repo := RoomRepo{c.db.C("rooms")}
	err := repo.Update(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusAccepted, body)
}

func (c *appContext) deleteRoomHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := RoomRepo{c.db.C("rooms")}
	err := repo.Delete(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	data := MessageSuccess{MessageInfo{Message: "Room has been deleted successfully"}}
	WriteSuccess(w, http.StatusAccepted, data)
}

func (c *appContext) roomsOfficeHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := RoomRepo{c.db.C("rooms")}
	rooms, err := repo.AllByOfficeId(params.ByName("office_id"))
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, rooms)
}

func main() {
	gotenv.Load()

	session, err := mgo.Dial("localhost")
	if err != nil {
		panic(err)
	}
	defer session.Close()
	session.SetMode(mgo.Monotonic, true)

	appC := appContext{session.DB("ivana")}
	commonHandlers := alice.New(context.ClearHandler, loggingHandler, recoverHandler, acceptHandler)
	router := NewRouter()

	// Routing

	router.Get("/offices/:id", commonHandlers.ThenFunc(appC.officeHandler))
	router.Patch("/offices/:id", commonHandlers.Append(contentTypeHandler, bodyHandler(OfficeResource{})).ThenFunc(appC.updateOfficeHandler))
	router.Delete("/offices/:id", commonHandlers.ThenFunc(appC.deleteOfficeHandler))
	router.Get("/offices", commonHandlers.ThenFunc(appC.officesHandler))
	router.Post("/offices", commonHandlers.Append(contentTypeHandler, bodyHandler(OfficeResource{})).ThenFunc(appC.createOfficeHandler))

	router.Get("/offices/:id/rooms", commonHandlers.ThenFunc(appC.roomsOfficeHandler))

	router.Get("/rooms/:id", commonHandlers.ThenFunc(appC.roomHandler))
	router.Patch("/rooms/:id", commonHandlers.Append(contentTypeHandler, bodyHandler(RoomResource{})).ThenFunc(appC.updateRoomHandler))
	router.Delete("/rooms/:id", commonHandlers.ThenFunc(appC.deleteRoomHandler))
	router.Get("/rooms", commonHandlers.ThenFunc(appC.roomsHandler))
	router.Post("/rooms", commonHandlers.Append(contentTypeHandler, bodyHandler(RoomResource{})).ThenFunc(appC.createRoomHandler))

	port := os.Getenv("PORT")
	msg := fmt.Sprintf("Listening at port %s", port)
	msgport := fmt.Sprintf(":%s", port)

	if os.Getenv("ENV") == "development" || os.Getenv("ENV") == "staging" {
		log.Println(msg)
	}
	log.Fatal(http.ListenAndServe(msgport, router))
}
