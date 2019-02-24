package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/gorilla/context"
	"github.com/jinzhu/now"
	"github.com/julienschmidt/httprouter"
	"github.com/justinas/alice"
	"github.com/subosito/gotenv"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
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

// Repo Venue

type Venue struct {
	Id        bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name      string        `json:"name"`
	Rooms     []Room        `json:"rooms,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type VenuesCollection struct {
	Data []Venue `json:"data"`
}

type VenueResource struct {
	Data Venue `json:"data"`
}

type VenueRepo struct {
	coll *mgo.Collection
}

func (r *VenueRepo) All() (VenuesCollection, error) {
	result := VenuesCollection{[]Venue{}}
	err := r.coll.Find(nil).All(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *VenueRepo) Find(id string) (VenueResource, error) {
	result := VenueResource{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *VenueRepo) Create(venue *Venue) error {
	id := bson.NewObjectId()
	venue.CreatedAt = time.Now()
	venue.UpdatedAt = time.Now()
	_, err := r.coll.UpsertId(id, venue)
	if err != nil {
		return err
	}

	venue.Id = id

	return nil
}

func (r *VenueRepo) Update(venue *Venue) error {
	venue.UpdatedAt = time.Now()
	err := r.coll.UpdateId(venue.Id, venue)
	if err != nil {
		return err
	}

	return nil
}

func (r *VenueRepo) Delete(id string) error {
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

// Venue Handlers

func (c *appContext) venuesHandler(w http.ResponseWriter, r *http.Request) {
	repo := VenueRepo{c.db.C("venues")}
	roomRepo := RoomRepo{c.db.C("rooms")}
	venues, err := repo.All()
	if err != nil {
		panic(err)
	}

	for idx, venue := range venues.Data {
		rooms, err := roomRepo.AllByVenueId(venue.Id.Hex())
		if err != nil {
			panic(err)
		}
		venues.Data[idx].Rooms = rooms.Data
	}

	WriteSuccess(w, http.StatusOK, venues)
}

func (c *appContext) venueHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := VenueRepo{c.db.C("venues")}
	venue, err := repo.Find(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, venue)
}

func (c *appContext) createVenueHandler(w http.ResponseWriter, r *http.Request) {
	body := context.Get(r, "body").(*VenueResource)
	repo := VenueRepo{c.db.C("venues")}
	err := repo.Create(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateVenueHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*VenueResource)
	body.Data.Id = bson.ObjectIdHex(params.ByName("id"))
	repo := VenueRepo{c.db.C("venues")}
	err := repo.Update(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusAccepted, body)
}

func (c *appContext) deleteVenueHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := VenueRepo{c.db.C("venues")}
	err := repo.Delete(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	data := MessageSuccess{MessageInfo{Message: "Venue has been deleted successfully"}}
	WriteSuccess(w, http.StatusAccepted, data)
}

// Repo Room

type Room struct {
	Id        bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name      string        `json:"name"`
	VenueId   string        `json:"venue_id"`
	Capacity  string        `json:"capacity"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
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
	room.CreatedAt = time.Now()
	room.UpdatedAt = time.Now()
	_, err := r.coll.UpsertId(id, room)
	if err != nil {
		return err
	}

	room.Id = id

	return nil
}

func (r *RoomRepo) Update(room *Room) error {
	room.UpdatedAt = time.Now()
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

func (r *RoomRepo) AllByVenueId(venueId string) (RoomsCollection, error) {
	result := RoomsCollection{[]Room{}}
	fmt.Println(venueId)
	err := r.coll.Find(bson.M{"venueid": venueId}).All(&result.Data)
	if err != nil {
		return result, err
	}
	fmt.Println(result)
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

func (c *appContext) roomsVenueHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := RoomRepo{c.db.C("rooms")}
	rooms, err := repo.AllByVenueId(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, rooms)
}

// Repo Appointment

type Appointment struct {
	Id              bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	AppointmentName string        `json:"appointment_name"`
	RoomId          string        `json:"room_id"`
	AppointmentDesc string        `json:"appointment_description"`
	GuestIds        []string      `json:"guest_ids"`
	Owner           bool          `json:"owner"`
	StartTime       time.Time     `json:"start_time"`
	EndTime         time.Time     `json:"end_time"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

type AppointmentsCollection struct {
	Data []Appointment `json:"data"`
}

type AppointmentResource struct {
	Data Appointment `json:"data"`
}

type AppointmentRepo struct {
	coll *mgo.Collection
}

func (r *AppointmentRepo) All() (AppointmentsCollection, error) {
	result := AppointmentsCollection{[]Appointment{}}
	beginningOfMonth := now.BeginningOfMonth()
	endOfMonth := now.EndOfMonth()
	err := r.coll.Find(bson.M{"start_time": bson.M{"$gte": beginningOfMonth, "$lte": endOfMonth}}).All(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *AppointmentRepo) Find(id string) (AppointmentResource, error) {
	result := AppointmentResource{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *AppointmentRepo) Create(appointment *Appointment) error {
	id := bson.NewObjectId()
	appointment.CreatedAt = time.Now()
	appointment.UpdatedAt = time.Now()
	_, err := r.coll.UpsertId(id, appointment)
	if err != nil {
		return err
	}

	appointment.Id = id

	return nil
}

func (r *AppointmentRepo) Update(appointment *Appointment) error {
	appointment.UpdatedAt = time.Now()
	err := r.coll.UpdateId(appointment.Id, appointment)
	if err != nil {
		return err
	}

	return nil
}

func (r *AppointmentRepo) Delete(id string) error {
	err := r.coll.RemoveId(bson.ObjectIdHex(id))
	if err != nil {
		return err
	}

	return nil
}

func (r *AppointmentRepo) Search(roomIds []string, owner bool, guestId string) (AppointmentsCollection, error) {
	result := AppointmentsCollection{[]Appointment{}}
	err := r.coll.Find(bson.M{
		"room_id": bson.M{
			"$in": roomIds,
		}, "$or": []bson.M{
			{"owner": owner},
			{"guest_ids": guestId},
		},
	}).All(&result.Data)
	if err != nil {
		return result, err
	}

	return result, nil
}

// Appointment Handlers

func (c *appContext) appointmentsHandler(w http.ResponseWriter, r *http.Request) {
	repo := AppointmentRepo{c.db.C("appointments")}
	appointments, err := repo.All()
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, appointments)
}

func (c *appContext) appointmentHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := AppointmentRepo{c.db.C("appointments")}
	appointment, err := repo.Find(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, appointment)
}

func (c *appContext) createAppointmentHandler(w http.ResponseWriter, r *http.Request) {
	body := context.Get(r, "body").(*AppointmentResource)
	repo := AppointmentRepo{c.db.C("appointments")}
	err := repo.Create(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateAppointmentHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*AppointmentResource)
	body.Data.Id = bson.ObjectIdHex(params.ByName("id"))
	repo := AppointmentRepo{c.db.C("appointments")}
	err := repo.Update(&body.Data)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusAccepted, body)
}

func (c *appContext) deleteAppointmentHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := AppointmentRepo{c.db.C("appointments")}
	err := repo.Delete(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	data := MessageSuccess{MessageInfo{Message: "Appointment has been deleted successfully"}}
	WriteSuccess(w, http.StatusAccepted, data)
}

func (c *appContext) searchAppointmentsHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	roomIds := r.URL.Query()["room_ids[]"]
	owner, _ := strconv.ParseBool(params.ByName("owner"))
	guestId := params.ByName("guest_id")
	repo := AppointmentRepo{c.db.C("appointments")}
	appointments, err := repo.Search(roomIds, owner, guestId)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusOK, appointments)
}

func main() {
	gotenv.Load()

	session, err := mgo.Dial("localhost")
	if err != nil {
		panic(err)
	}
	defer session.Close()
	session.SetMode(mgo.Monotonic, true)

	// Index
	appC := appContext{session.DB("ivana")}
	commonHandlers := alice.New(context.ClearHandler, loggingHandler, recoverHandler, acceptHandler)
	router := NewRouter()

	// Routing

	router.Get("/venues/:id", commonHandlers.ThenFunc(appC.venueHandler))
	router.Patch("/venues/:id", commonHandlers.Append(contentTypeHandler, bodyHandler(VenueResource{})).ThenFunc(appC.updateVenueHandler))
	router.Delete("/venues/:id", commonHandlers.ThenFunc(appC.deleteVenueHandler))
	router.Get("/venues", commonHandlers.ThenFunc(appC.venuesHandler))
	router.Post("/venues", commonHandlers.Append(contentTypeHandler, bodyHandler(VenueResource{})).ThenFunc(appC.createVenueHandler))

	router.Get("/venues/:id/rooms", commonHandlers.ThenFunc(appC.roomsVenueHandler))

	router.Get("/rooms/:id", commonHandlers.ThenFunc(appC.roomHandler))
	router.Patch("/rooms/:id", commonHandlers.Append(contentTypeHandler, bodyHandler(RoomResource{})).ThenFunc(appC.updateRoomHandler))
	router.Delete("/rooms/:id", commonHandlers.ThenFunc(appC.deleteRoomHandler))
	router.Get("/rooms", commonHandlers.ThenFunc(appC.roomsHandler))
	router.Post("/rooms", commonHandlers.Append(contentTypeHandler, bodyHandler(RoomResource{})).ThenFunc(appC.createRoomHandler))

	router.Get("/appointments/:id", commonHandlers.ThenFunc(appC.appointmentHandler))
	router.Patch("/appointments/:id", commonHandlers.Append(contentTypeHandler, bodyHandler(AppointmentResource{})).ThenFunc(appC.updateAppointmentHandler))
	router.Delete("/appointments/:id", commonHandlers.ThenFunc(appC.deleteAppointmentHandler))
	router.Get("/appointments", commonHandlers.ThenFunc(appC.appointmentsHandler))
	router.Post("/appointments", commonHandlers.Append(contentTypeHandler, bodyHandler(AppointmentResource{})).ThenFunc(appC.createAppointmentHandler))
	router.Get("/search-appointments", commonHandlers.ThenFunc(appC.searchAppointmentsHandler))

	port := os.Getenv("PORT")
	msg := fmt.Sprintf("Listening at port %s", port)
	msgport := fmt.Sprintf(":%s", port)

	if os.Getenv("ENV") == "development" || os.Getenv("ENV") == "staging" {
		log.Println(msg)
	}
	log.Fatal(http.ListenAndServe(msgport, router))
}
