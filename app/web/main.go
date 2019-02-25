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
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
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

// func acceptHandler(next http.Handler) http.Handler {
// 	fn := func(w http.ResponseWriter, r *http.Request) {
// 		if r.Header.Get("Accept") != "application/vnd.api+json" {
// 			WriteError(w, ErrNotAcceptable)
// 			return
// 		}

// 		next.ServeHTTP(w, r)
// 	}

// 	return http.HandlerFunc(fn)
// }

// func contentTypeHandler(next http.Handler) http.Handler {
// 	fn := func(w http.ResponseWriter, r *http.Request) {
// 		if r.Header.Get("Content-Type") != "application/vnd.api+json" {
// 			WriteError(w, ErrUnsupportedMediaType)
// 			return
// 		}

// 		next.ServeHTTP(w, r)
// 	}

// 	return http.HandlerFunc(fn)
// }

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
	Id    bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name  string        `json:"name"`
	Rooms []Room        `json:"rooms,omitempty"`
}

type VenueRepo struct {
	coll *mgo.Collection
}

func (r *VenueRepo) All() ([]Venue, error) {
	result := []Venue{}
	err := r.coll.Find(nil).All(&result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *VenueRepo) Find(id string) (Venue, error) {
	result := Venue{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *VenueRepo) Create(venue *Venue) error {
	id := bson.NewObjectId()
	_, err := r.coll.UpsertId(id, venue)
	if err != nil {
		return err
	}

	venue.Id = id

	return nil
}

func (r *VenueRepo) Update(venue *Venue) error {
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

	for idx, venue := range venues {
		rooms, err := roomRepo.AllByVenueId(venue.Id.Hex())
		if err != nil {
			panic(err)
		}
		venues[idx].Rooms = rooms
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
	body := context.Get(r, "body").(*Venue)
	repo := VenueRepo{c.db.C("venues")}
	err := repo.Create(body)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateVenueHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*Venue)
	body.Id = bson.ObjectIdHex(params.ByName("id"))
	repo := VenueRepo{c.db.C("venues")}
	err := repo.Update(body)
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
	Id       bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name     string        `json:"name"`
	VenueId  string        `json:"venue_id"`
	Capacity string        `json:"capacity"`
}

type RoomRepo struct {
	coll *mgo.Collection
}

func (r *RoomRepo) All() ([]Room, error) {
	result := []Room{}
	err := r.coll.Find(nil).All(&result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *RoomRepo) Find(id string) (Room, error) {
	result := Room{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result)
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

func (r *RoomRepo) AllByVenueId(venueId string) ([]Room, error) {
	result := []Room{}
	err := r.coll.Find(bson.M{"venueid": venueId}).All(&result)
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
	body := context.Get(r, "body").(*Room)
	repo := RoomRepo{c.db.C("rooms")}
	err := repo.Create(body)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateRoomHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println(context.Get(r, "body"))
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*Room)
	body.Id = bson.ObjectIdHex(params.ByName("id"))
	repo := RoomRepo{c.db.C("rooms")}
	err := repo.Update(body)
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

// Repo Event

type Event struct {
	Id          bson.ObjectId `json:"id,omitempty" bson:"_id,omitempty"`
	Name        string        `json:"name"`
	LocationID  string        `json:"location_id"`
	Location    string        `json:"location"`
	Description string        `json:"description"`
	Guests      []string      `json:"guests"`
	Owner       string        `json:"owner"`
	StartTime   time.Time     `json:"start_time"`
	EndTime     time.Time     `json:"end_time"`
}

type EventResponse struct {
	Id          bson.ObjectId `json:"id,omitempty"`
	Name        string        `json:"name"`
	LocationID  string        `json:"location_id"`
	Location    string        `json:"location"`
	Description string        `json:"description"`
	Guests      []string      `json:"guests"`
	Owner       string        `json:"owner"`
	Date        int           `json:"date"`
	Month       int           `json:"month"`
	Year        int           `json:"year"`
	StartHour   int           `json:"start_hour"`
	StartMinute int           `json:"start_minute"`
	EndHour     int           `json:"end_hour"`
	EndMinute   int           `json:"end_minute"`
}

type EventRepo struct {
	coll *mgo.Collection
}

func (r *EventRepo) All(start_time time.Time, end_time time.Time) ([]Event, error) {
	result := []Event{}
	err := r.coll.Find(bson.M{"starttime": bson.M{"$gte": start_time, "$lte": end_time}}).All(&result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *EventRepo) Find(id string) (Event, error) {
	result := Event{}
	err := r.coll.FindId(bson.ObjectIdHex(id)).One(&result)
	if err != nil {
		return result, err
	}

	return result, nil
}

func (r *EventRepo) Create(event *Event) error {
	id := bson.NewObjectId()
	_, err := r.coll.UpsertId(id, event)
	if err != nil {
		return err
	}

	event.Id = id

	return nil
}

func (r *EventRepo) Update(event *Event) error {
	err := r.coll.UpdateId(event.Id, event)
	if err != nil {
		return err
	}

	return nil
}

func (r *EventRepo) Delete(id string) error {
	err := r.coll.RemoveId(bson.ObjectIdHex(id))
	if err != nil {
		return err
	}

	return nil
}

func (r *EventRepo) Search(roomIds []string, owner string, guests string) ([]Event, error) {
	result := []Event{}
	beginningOfWeek := now.BeginningOfWeek()
	endOfWeek := now.EndOfWeek()

	err := r.coll.Find(bson.M{
		"room_id": bson.M{
			"$in": roomIds,
		}, "$or": []bson.M{
			{"owner": owner},
			{"guests": guests},
		}, "start_time": bson.M{
			"$gte": beginningOfWeek,
			"$lte": endOfWeek,
		},
	}).All(&result)
	if err != nil {
		return result, err
	}

	return result, nil
}

// Event Handlers

func (c *appContext) eventsHandler(w http.ResponseWriter, r *http.Request) {
	repo := EventRepo{c.db.C("events")}
	loc := time.FixedZone("UTC+7", 7*60*60)
	start_time := now.BeginningOfWeek()
	if r.URL.Query().Get("start_time") != "" {
		start_time, _ = time.Parse(time.RFC3339, r.URL.Query().Get("start_time"))
		start_time = start_time.In(loc)
	}
	end_time := now.EndOfWeek()
	if r.URL.Query().Get("end_time") != "" {
		end_time, _ = time.Parse(time.RFC3339, r.URL.Query().Get("end_time"))
		end_time = end_time.In(loc)
	}

	events, err := repo.All(start_time, end_time)
	if err != nil {
		panic(err)
	}

	// results := []EventResponse{}
	// for _, event := range events {
	// 	results = append(results, EventResponse{
	// 		Id:          event.Id,
	// 		Name:        event.Name,
	// 		LocationID:  event.LocationID,
	// 		Location:    event.Location,
	// 		Description: event.Description,
	// 		Guests:      event.Guests,
	// 		Owner:       event.Owner,
	// 		Date:        event.StartTime.Day(),
	// 		Month:       int(event.StartTime.Month()),
	// 		Year:        event.StartTime.Year(),
	// 		StartHour:   event.StartTime.Hour(),
	// 		StartMinute: event.StartTime.Minute(),
	// 		EndHour:     event.EndTime.Hour(),
	// 		EndMinute:   event.EndTime.Minute(),
	// 	})
	// }
	// WriteSuccess(w, http.StatusOK, results)

	WriteSuccess(w, http.StatusOK, events)
}

func (c *appContext) eventHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := EventRepo{c.db.C("events")}
	event, err := repo.Find(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	eventRes := EventResponse{
		Id:          event.Id,
		Name:        event.Name,
		LocationID:  event.LocationID,
		Location:    event.Location,
		Description: event.Description,
		Guests:      event.Guests,
		Owner:       event.Owner,
		Date:        event.StartTime.Day(),
		Month:       int(event.StartTime.Month()),
		Year:        event.StartTime.Year(),
		StartHour:   event.StartTime.Hour(),
		StartMinute: event.StartTime.Minute(),
		EndHour:     event.EndTime.Hour(),
		EndMinute:   event.EndTime.Minute(),
	}

	WriteSuccess(w, http.StatusOK, eventRes)
}

func (c *appContext) createEventHandler(w http.ResponseWriter, r *http.Request) {
	loc := time.FixedZone("UTC+7", 7*60*60)
	body := context.Get(r, "body").(*EventResponse)
	event := Event{
		Name:        body.Name,
		LocationID:  body.LocationID,
		Location:    body.Location,
		Description: body.Description,
		Guests:      body.Guests,
		Owner:       body.Owner,
		StartTime:   time.Date(body.Year, time.Month(body.Month), body.Date, body.StartHour, body.StartMinute, 0, 0, loc),
		EndTime:     time.Date(body.Year, time.Month(body.Month), body.Date, body.EndHour, body.EndMinute, 0, 0, loc),
	}

	repo := EventRepo{c.db.C("events")}
	err := repo.Create(&event)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusCreated, body)
}

func (c *appContext) updateEventHandler(w http.ResponseWriter, r *http.Request) {
	loc := time.FixedZone("UTC+7", 7*60*60)
	params := context.Get(r, "params").(httprouter.Params)
	body := context.Get(r, "body").(*EventResponse)
	event := Event{
		Id:          bson.ObjectIdHex(params.ByName("id")),
		Name:        body.Name,
		LocationID:  body.LocationID,
		Location:    body.Location,
		Description: body.Description,
		Guests:      body.Guests,
		Owner:       body.Owner,
		StartTime:   time.Date(body.Year, time.Month(body.Month), body.Date, body.StartHour, body.StartMinute, 0, 0, loc),
		EndTime:     time.Date(body.Year, time.Month(body.Month), body.Date, body.EndHour, body.EndMinute, 0, 0, loc),
	}

	repo := EventRepo{c.db.C("events")}
	err := repo.Update(&event)
	if err != nil {
		panic(err)
	}

	WriteSuccess(w, http.StatusAccepted, body)
}

func (c *appContext) deleteEventHandler(w http.ResponseWriter, r *http.Request) {
	params := context.Get(r, "params").(httprouter.Params)
	repo := EventRepo{c.db.C("events")}
	err := repo.Delete(params.ByName("id"))
	if err != nil {
		panic(err)
	}

	data := MessageSuccess{MessageInfo{Message: "Event has been deleted successfully"}}
	WriteSuccess(w, http.StatusAccepted, data)
}

// func (c *appContext) searchEventsHandler(w http.ResponseWriter, r *http.Request) {
// 	params := context.Get(r, "params").(httprouter.Params)
// 	roomIds := r.URL.Query()["room_ids[]"]
// 	owner := params.ByName("owner")
// 	guests := params.ByName("guests")
// 	fmt.Println(roomIds)
// 	fmt.Println(owner)
// 	fmt.Println(owner)
// 	repo := EventRepo{c.db.C("events")}
// 	events, err := repo.Search(roomIds, owner, guests)
// 	if err != nil {
// 		panic(err)
// 	}

// 	result := []Event{[]EventResponse{}}
// 	for idx, event := range events {
// 		result[idx] = EventResponse{
// 			Id:          event.Id,
// 			Name:        event.Name,
// 			LocationID:  event.LocationID,
// 			Location:    event.Location,
// 			Description: event.Description,
// 			Guests:      event.Guests,
// 			Owner:       event.Owner,
// 			Date:        event.StartTime.Day(),
// 			Month:       int(event.StartTime.Month()),
// 			Year:        event.StartTime.Year(),
// 			StartHour:   event.StartTime.Hour(),
// 			StartMinute: event.StartTime.Minute(),
// 			EndHour:     event.EndTime.Hour(),
// 			EndMinute:   event.EndTime.Minute(),
// 		}
// 	}

// 	WriteSuccess(w, http.StatusOK, result)
// }

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
	commonHandlers := alice.New(context.ClearHandler, loggingHandler, recoverHandler)
	router := NewRouter()

	// Routing

	router.Get("/venues/:id", commonHandlers.ThenFunc(appC.venueHandler))
	router.Patch("/venues/:id", commonHandlers.Append(bodyHandler(Venue{})).ThenFunc(appC.updateVenueHandler))
	router.Delete("/venues/:id", commonHandlers.ThenFunc(appC.deleteVenueHandler))
	router.Get("/venues", commonHandlers.ThenFunc(appC.venuesHandler))
	router.Post("/venues", commonHandlers.Append(bodyHandler(Venue{})).ThenFunc(appC.createVenueHandler))

	router.Get("/venues/:id/rooms", commonHandlers.ThenFunc(appC.roomsVenueHandler))

	router.Get("/rooms/:id", commonHandlers.ThenFunc(appC.roomHandler))
	router.Patch("/rooms/:id", commonHandlers.Append(bodyHandler(Room{})).ThenFunc(appC.updateRoomHandler))
	router.Delete("/rooms/:id", commonHandlers.ThenFunc(appC.deleteRoomHandler))
	router.Get("/rooms", commonHandlers.ThenFunc(appC.roomsHandler))
	router.Post("/rooms", commonHandlers.Append(bodyHandler(Room{})).ThenFunc(appC.createRoomHandler))

	router.Get("/events/:id", commonHandlers.ThenFunc(appC.eventHandler))
	router.Patch("/events/:id", commonHandlers.Append(bodyHandler(EventResponse{})).ThenFunc(appC.updateEventHandler))
	router.Delete("/events/:id", commonHandlers.ThenFunc(appC.deleteEventHandler))
	router.Post("/events", commonHandlers.Append(bodyHandler(EventResponse{})).ThenFunc(appC.createEventHandler))
	router.Get("/events", commonHandlers.ThenFunc(appC.eventsHandler))

	port := os.Getenv("PORT")
	msg := fmt.Sprintf("Listening at port %s", port)
	msgport := fmt.Sprintf(":%s", port)

	if os.Getenv("ENV") == "development" || os.Getenv("ENV") == "staging" {
		log.Println(msg)
	}
	log.Fatal(http.ListenAndServe(msgport, router))
}
